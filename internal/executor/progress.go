package executor

func (e Executor) reportProgress(progress Progress) {
	if e.Progress != nil {
		e.Progress(progress)
	}
}
