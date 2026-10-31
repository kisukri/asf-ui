FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/asf-panel . && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/asf-panel /asf-panel
COPY --from=build --chown=65532:65532 /out/data /data
ENV LISTEN_ADDR=0.0.0.0:8000 \
    DATABASE_PATH=/data/panel.db
VOLUME ["/data"]
EXPOSE 8000
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=6s --start-period=10s --retries=3 CMD ["/asf-panel", "healthcheck"]
ENTRYPOINT ["/asf-panel"]
CMD ["serve"]
