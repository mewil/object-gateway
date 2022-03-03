FROM golang:1.17-alpine AS build
ENV CGO_ENABLED=0
COPY . /go/src/github.com/mewil/object-gateway
WORKDIR /go/src/github.com/mewil/object-gateway
RUN go mod download
RUN go install .
RUN adduser -D -g '' user

FROM scratch AS object-gateway
LABEL Author="Michael Wilson"
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /go/bin/object-gateway /bin/object-gateway
USER user
ENTRYPOINT ["/bin/object-gateway"]