package controller

import (
	"context"
	"errors"
	"time"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
)

// Database evidence must not consume an unbounded part of a shutdown window.
// The bounded store latches a timed-out operation until this reconcile ends.
const shutdownAuditIOTimeout = time.Second

func (r *ShutdownFlowReconciler) openExecutionAuditStore(ctx context.Context, cluster *powerv1alpha1.PowerManagementCluster) (audit.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var store audit.Store
	var err error
	if managementClusterStorageReady(cluster) {
		openCtx, cancel := context.WithTimeout(ctx, shutdownAuditIOTimeout)
		store, err = r.storageConnector().OpenAuditStore(openCtx, cluster)
		cancel()
	} else {
		err = errors.New("PostgreSQL audit storage is not ready")
	}
	if store == nil && err == nil {
		err = errors.New("audit connector returned no store")
	}
	if err != nil {
		if store != nil {
			_ = store.Close()
		}
		if !cluster.Spec.Storage.AuditSpool.Enabled {
			return nil, err
		}
		store = audit.UnavailableStore(err)
	}
	return audit.NewBoundedStore(store, shutdownAuditIOTimeout)
}
