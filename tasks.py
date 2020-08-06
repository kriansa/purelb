import os
import semver
import sys

from invoke import run, task
from invoke.exceptions import Exit

all_binaries = set(["controller-pool",
                    "controller-acnodal",
                    "speaker-acnodal",
                    "speaker-local"])


def _check_binaries(binaries):
    out = set()
    for binary in binaries:
        if binary == "all":
            out |= all_binaries
        elif binary not in all_binaries:
            print("Unknown binary {}".format(binary))
            print("Known binaries: {}".format(", ".join(sorted(all_binaries))))
            sys.exit(1)
        else:
            out.add(binary)
    if not out:
        out |= all_binaries
    return list(sorted(out))


@task(iterable=["binaries"],
      help={
          "binaries": "binaries to build. One or more of {}, or 'all'".format(", ".join(sorted(all_binaries))),
          "tag": "docker image tag prefix to use. Default 'dev'.",
          "docker-user": "docker user under which to tag the images. Default 'metallb'.",
      })
def build(ctx, binaries, tag="dev", docker_user="metallb"):
    """Build MetalLB docker images."""
    binaries = _check_binaries(binaries)

    run("go test ./...")  # run the unit tests first

    commit = run("git describe --dirty --always", hide=True).stdout.strip()
    branch = run("git rev-parse --abbrev-ref HEAD", hide=True).stdout.strip()

    for bin in binaries:
        run("docker build -t {user}/{bin}:{tag} --build-arg cmd={bin}"
            " --build-arg commit={commit} --build-arg branch={branch}"
            " -f build/package/Dockerfile.{bin} .".format(
                user=docker_user,
                bin=bin,
                tag=tag,
                commit=commit,
                branch=branch),
            echo=True)


@task(iterable=["binaries"],
      help={
          "binaries": "binaries to build. One or more of {}, or 'all'".format(", ".join(sorted(all_binaries))),
          "tag": "docker image tag prefix to use. Default 'dev'.",
          "docker-user": "docker user under which to tag the images. Default 'metallb'.",
      })
def push(ctx, binaries, tag="dev", docker_user="metallb"):
    """Build and push docker images to registry."""
    binaries = _check_binaries(binaries)

    for bin in binaries:
        build(ctx, binaries=[bin], tag=tag, docker_user=docker_user)
        run("docker push {user}/{bin}:{tag}".format(
            user=docker_user,
            bin=bin,
            tag=tag),
            echo=True)


@task(help={
    "name": "name of the kind cluster to use.",
})
def release(ctx, version, skip_release_notes=False):
    """Tag a new release."""
    status = run("git status --porcelain", hide=True).stdout.strip()
    if status != "":
        raise Exit(message="git checkout not clean, cannot release")

    version = semver.parse_version_info(version)
    is_patch_release = version.patch != 0

    # Move HEAD to the correct release branch - either a new one, or
    # an existing one.
    if is_patch_release:
        run("git checkout v{}.{}".format(version.major, version.minor), echo=True)
    else:
        run("git checkout -b v{}.{}".format(version.major, version.minor), echo=True)

    # Update the manifests with the new version
    run("perl -pi -e 's,image: metallb/speaker:.*,image: metallb/speaker:v{},g' deployments/metallb.yaml".format(version), echo=True)
    run("perl -pi -e 's,image: metallb/controller:.*,image: metallb/controller:v{},g' deployments/metallb.yaml".format(version), echo=True)

    # Update the version embedded in the binary
    run("perl -pi -e 's/version\s+=.*/version = \"{}\"/g' internal/version/version.go".format(version), echo=True)
    run("gofmt -w internal/version/version.go", echo=True)

    run("git commit -a -m 'Automated update for release v{}'".format(version), echo=True)
    run("git checkout main", echo=True)
