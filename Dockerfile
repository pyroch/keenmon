FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /keenmon .

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
    && addgroup -S keenmon \
    && adduser -S -G keenmon keenmon
WORKDIR /app
COPY --from=build /keenmon /usr/local/bin/keenmon
USER keenmon
EXPOSE 8758
ENTRYPOINT ["keenmon"]
