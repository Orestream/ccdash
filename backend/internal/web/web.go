// Package web stages the production frontend bundle (copied here by `make
// build`) and embeds it into the binary when compiled with `-tags prod`. The
// default (untagged) build has no embed, so `go build ./...` and the test
// suite work on a fresh checkout without ever running `npm run build`.
package web
