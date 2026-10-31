# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/elko-feedback ./cmd/elko-feedback

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/elko-feedback /elko-feedback
ENV ELKO_FEEDBACK_DB=/data/feedback.db ELKO_FEEDBACK_ADDR=:8080
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/elko-feedback"]
CMD ["serve"]
