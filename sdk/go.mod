// Module github.com/orlandoburli/apiary/sdk is the public Go SDK for writing
// Apiary plugins. It is a separate module from the daemon so that third-party
// plugins depend only on the protocol, never on the daemon's dependency graph.
//
// It is tagged independently of the daemon: SDK releases use tags of the form
// sdk/vX.Y.Z, daemon releases use vX.Y.Z.
//
// Dependencies: standard library only. Keep it that way.
module github.com/orlandoburli/apiary/sdk

go 1.24
