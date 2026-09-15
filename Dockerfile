FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/tos-tag ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
COPY --from=build /out/tos-tag /usr/local/bin/tos-tag
USER nonroot:nonroot
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/tos-tag"]
