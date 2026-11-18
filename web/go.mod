// This file only keeps web/ (and web/node_modules, which holds Go files from
// some npm packages) out of the main module, so `go vet ./...` and
// `go test ./...` at the repo root never scan it. There is no Go code here.
module github.com/KarvinNanda/relaydeck/web

go 1.27
