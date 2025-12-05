FROM public.ecr.aws/docker/library/golang:alpine as builder

RUN go env -w CGO_ENABLED=0

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN go build -trimpath -ldflags "-s -w -extldflags '-static -fpic'"  -o npd-node-replace main.go

FROM public.ecr.aws/docker/library/alpine

WORKDIR /app

COPY --from=builder --chmod=755 /app/npd-node-replace /app/npd-node-replace

ENTRYPOINT ["/app/npd-node-replace"]