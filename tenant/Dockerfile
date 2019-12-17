# Build the manager binary
FROM golang:1.10.3 as builder

# Copy in the go src
WORKDIR /go/src/github.com/diamanticom/multi-tenancy/tenant
COPY pkg/    pkg/
COPY cmd/    cmd/
COPY vendor/ vendor/
COPY kubernetes_add_service_account_kubeconfig.sh kubernetes_add_service_account_kubeconfig.sh 
COPY permissions-template.yaml permissions-template.yaml 
# 

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager github.com/diamanticom/multi-tenancy/tenant/cmd/manager

# Copy the controller-manager into a thin image
FROM ubuntu:latest
WORKDIR /
COPY --from=builder /go/src/github.com/diamanticom/multi-tenancy/tenant/manager .
ENTRYPOINT ["/manager"]
