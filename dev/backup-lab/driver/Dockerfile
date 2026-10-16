# The lab's toolbox: curl, jq, the Postgres and SQLite command-line clients, and bash.
# Built once with network access; at run time it sits on the lab's internal network only.
FROM alpine:3.23
RUN apk add --no-cache bash curl jq coreutils postgresql18-client sqlite tzdata
CMD ["sleep", "infinity"]
