FROM public.ecr.aws/docker/library/golang:1.24.5 as builder

WORKDIR /app

COPY . .

RUN CGO_ENABLED=0 go build -o npd-node-replace main.go

FROM public.ecr.aws/docker/library/alpine:3.20

WORKDIR /app

COPY --from=builder /app/npd-node-replace .

CMD ["./npd-node-replace"]