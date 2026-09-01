FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY pkg/ pkg/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/scaler ./cmd/scaler

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/scaler /scaler

USER nonroot:nonroot
EXPOSE 9001 9090

ENTRYPOINT ["/scaler"]
