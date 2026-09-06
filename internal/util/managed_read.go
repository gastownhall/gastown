package util

// ManagedReadEnv marks a synchronous CLI read whose entire process group is
// owned and cancelled by a dashboard parent. Nested commands must not detach
// from that group. Ordinary CLI commands retain their existing process policy.
const ManagedReadEnv = "GT_MANAGED_READ"
