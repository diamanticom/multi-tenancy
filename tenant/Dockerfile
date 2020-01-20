# Build the manager binary
FROM golang:1.13 as builder

# Copy in the go src
WORKDIR /go/src/github.com/diamanticom/multi-tenancy/tenant
COPY pkg/    pkg/
COPY tenantdb/    tenantdb/
COPY clusterapi/    clusterapi/
COPY tools/    tools/
COPY cmd/    cmd/
COPY vendor/ vendor/
# 

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager github.com/diamanticom/multi-tenancy/tenant/cmd/manager

# Copy the controller-manager into a thin image
FROM ubuntu:latest
WORKDIR /
COPY --from=builder /go/src/github.com/diamanticom/multi-tenancy/tenant/manager .
COPY --from=builder /go/src/github.com/diamanticom/multi-tenancy/tenant/tools .
RUN chmod +x kubernetes_add_service_account_kubeconfig.sh
EXPOSE 8090
ENTRYPOINT ["/manager"]
