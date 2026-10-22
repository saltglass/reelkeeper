// A separate module, so the fake Usenet never ends up in Reelkeeper's binary
// and isn't part of `go test ./...` or the lint of the app itself.
module github.com/saltglass/reelkeeper/dev/fakenews

go 1.27.1
