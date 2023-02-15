GO = go

buildfarm: *.go cmd/buildfarm/*.go emulation/*.go vendor/modules.txt
	$(GO) build ./cmd/buildfarm

clean:
	$(RM) buildfarm
