# Manual comparison fixture

This deliberately small project is copied into two identical Git repositories by
`scripts/manual-compare.ps1`. The evaluation prompt asks an agent to add an HTTP-backed
use case while the architectural rules prohibit application code from importing
`net/http` directly.
