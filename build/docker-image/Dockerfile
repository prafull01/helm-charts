# Build the manager binary
FROM golang:1.15.1 as base

WORKDIR /workspace

# Copy the go source
COPY ./cmd/main.go main.go

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on GOFLAGS=-mod=vendor go build -a -o manager main.go

FROM registry.access.redhat.com/ubi7/ubi:7.9 as final
WORKDIR /

ARG RUN_ID
ARG RUN_NUMBER
LABEL version="${RUN_ID}"
LABEL release="${RUN_NUMBER}"
COPY --from=base /workspace/manager .

ENTRYPOINT ["/manager"]
