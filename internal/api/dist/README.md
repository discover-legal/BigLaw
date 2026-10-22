`make build` copies the built UI (`ui/dist`) here before `go build`, so the binary serves it at `/`.
Everything here but this file is ignored by git; without a built UI, `/` explains how to build it.
