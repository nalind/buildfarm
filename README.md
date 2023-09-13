podman buildfarm/team/crew/bunch [![Cirrus CI Status](https://img.shields.io/cirrus/github/nalind/buildfarm/main)](https://cirrus-ci.com/github/nalind/buildfarm/main)
==
(still workshopping the name)

**This repository is a PoC for a feature in [podman](https://github.com/containers/podman), and once the feature arrives in a podman release, this repository will be archived.**

What are we doing here?
--
We're living in a future when building container images for just one target architecture isn't always enough.  An image built in one place can may be expected to run as a container in clouds, on workstations, on smaller/edge/embedded devices, and in places I haven't thought to list, and those devices won't always share a common CPU architecture.

What is this not?
--
`podman build` has been able to accept a list of values to its `--platform` option for some time now.  When invoked with a list of platforms and with its `--manifest` option, `podman` will build an image for multiple target platforms and then create a manifest list that enumerates the various newly-built architecture-specific images.

They can then all be pushed, as a group, to a registry by `podman manifest push --all`.

If the build host has the `qemu-user-static` package or an equivalent installed, `RUN` instructions in Dockerfiles won't even cause builds to fail with "`Exec format error`" errors.

This describes things `podman` does right now.  This... is not that.

What is this?
--
`podman` has a notion of "system connections", by which one copy of `podman` (more often, `podman-remote` running on a non-Linux system) "drives" a copy of `podman` running on a remote Linux system, connecting to that system via an SSH tunnel.  This feature is how `podman machine` makes the VMs it manages available to `podman` or `podman-remote`. 

Instead of performing the build locally, the way `podman build` does, this tool ships the build context off over a system connection, and when the image build is complete, it pulls the new image back over that connection into local storage.

It actually expects to perform builds using multiple system connections, and after they've all succeeded, it will generate a manifest list that includes the images that it has just built.

If the `podman` instances that it uses for performing builds are running on different architectures, it will mark their results correctly.

They can then all be pushed, as a group, to a registry by `podman manifest push --all`.

It's intentional that the final push step is the same as it would have been after `podman build --manifest`.

What does that look like?
--
Run everything locally using emulation (podman 4.0 and later):
```bash!
> : setup that only needs to happen once
> sudo dnf install qemu-user-static
> : do the thing
> podman build --platforms=linux/arm64,linux/amd64 --manifest registry.tld/my/repository:tag .
> podman manifest push --all registry.tld/my/repository:tag
```
Notes:
* Emulation works at the instruction level, so binaries which are run using emulation still see the host's true CPU information in `/proc/cpuinfo`, which can confuse them.
* `qemu-user` emulators use multiple threads, and this can cause certain system calls which they make on behalf of single-threaded binaries that they are interpreting to fail in ways those binaries do not expect.
* `qemu-user-static` emulators typically are not registered with the kernel's `binfmt_misc` feature with the flag ("C") that would be required for allowing interpreted setuid/setgid binaries to run setuid/setgid.

Use remote machines to do the heavy lifting (this repository):
```bash!
> : setup that only needs to happen once
> ssh f37box systemctl --user enable podman.socket
> podman system connection add f37 ssh://f37box
> podman machine init
> podman machine start
> : do the thing
> buildfarm --node f37,podman-machine-default build -t registry.tld/my/repository:tag .
> podman manifest push --all registry.tld/my/repository:tag
```
Notes:
* By default, a build farm will build images for all architectures which its nodes are able to run natively.
* If multiple system connections in a build farm can build for the same target architecture, one of them is chosen to do the work, and the other is left idle.  We'll want to make that scheduler smarter.
* The `--node` option instructs the tool to use an ad-hoc group of system connections as a build farm. 
* The `--farm` option instructs the tool to use a named set of system connections as a build farm.  Storing this information in `containers.conf(5)` requires adding to its format, but that format is maintained elsewhere, so it is not usably implemented here.
* When neither `--node` nor `--farm` is specified, the tool uses an ad-hoc farm which includes every known system connection.
