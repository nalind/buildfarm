GO = go

buildfarm: *.go cmd/buildfarm/*.go emulation/*.go
	$(GO) build ./cmd/buildfarm
