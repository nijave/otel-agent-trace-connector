FROM alpine:3.22
RUN apk add --no-cache curl jq
COPY run.sh /run.sh
RUN chmod +x /run.sh
ENTRYPOINT ["/run.sh"]
