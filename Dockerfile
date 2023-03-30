FROM golang:1.19-buster AS build

COPY . /src
WORKDIR /src
RUN go build

FROM debian:buster
COPY --from=build /src/beacon-cache-proxy /bin/beacon-cache-proxy
ENTRYPOINT ["/bin/beacon-cache-proxy"]
