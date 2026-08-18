# Guides

Components: Foundation & Documentation.

The judgement calls the operator cannot make for you, in the order you hit them. Each page states
what hangs on the answer, and none of them are optional in the sense that skipping one leaves a
default — skipping one leaves a gap that surfaces during a power failure.

1. [Preparing the hardware](prepare-your-hardware.md) — wiring, network reachability, and checking
   that what you wrote down matches the room. The step with no software fallback.
2. [Modeling your topology](model-your-topology.md) — turning real wiring into `feeds` and `carries`
   edges, and what each one changes.
3. [Assigning shutdown tiers](assign-shutdown-tiers.md) — the ordering vocabulary, and how to pick
   numbers you will not want to renumber later.
4. [Choosing what is last-ditch](choose-last-ditch-workloads.md) — what must still be running while
   everything else stops, and the cost of naming too much.
5. [Setting a tier-overrun policy](set-tier-overrun-policy.md) — what to do when a tier runs past its
   budget and the battery does not wait.
6. [Enabling actuation](enable-actuation.md) — the four approval gates, what each one turns on, and
   how to prove the cluster can really halt a node.

Steps 1 through 5 change nothing about how the cluster behaves. Step 6 is the one that does.
