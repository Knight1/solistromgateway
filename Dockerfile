# Build the binary in a throwaway stage, then ship it on nothing at all.
#
# The base image is pinned by digest so a build is reproducible and cannot be
# changed under us by a retagged image. Dependabot already watches the docker
# ecosystem for this repository, so it will raise a pull request when the pin
# falls behind.
FROM golang:1.27-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build

# The only thing needed from the build image is the CA bundle, which the final
# stage copies out so HTTPS to the push API can be verified.
RUN apk add --no-cache ca-certificates

WORKDIR /src
COPY . .

# Pass the version in, since the .git directory is deliberately kept out of the
# build context and the binary therefore cannot stamp itself:
#   docker build --build-arg VERSION="$(git describe --tags --always --dirty)" .
ARG VERSION=""

# CGO off produces a static binary that needs no libc, which is what makes the
# scratch stage below possible. -trimpath keeps build paths out of the binary,
# -s -w drop the symbol and DWARF tables, and -buildvcs=false makes the build
# the same whether or not a repository happens to be in the context.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /solistromgateway \
      ./cmd/solistromgateway

# Nothing but the binary and the certificates: no shell, no package manager, no
# libc, nothing to exec if the process is ever compromised.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /solistromgateway /solistromgateway

# nobody. scratch has no /etc/passwd, so the id has to be numeric.
USER 65534:65534

# The program writes nothing to disk and listens on no port. It only makes
# outbound requests, so no EXPOSE and no volumes of its own.
ENTRYPOINT ["/solistromgateway"]
CMD ["-config", "/config/config.json"]
