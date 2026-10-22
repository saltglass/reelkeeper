# The made-up Usenet for the dev stack (see main.go). Never published.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -o /fakenews .

FROM alpine:3.22
# par2 makes the repair files that `fakenews make` posts with each release.
RUN apk add --no-cache par2cmdline
COPY --from=build /fakenews /usr/local/bin/fakenews
VOLUME /data
EXPOSE 119 8080
ENTRYPOINT ["fakenews"]
CMD ["serve"]
