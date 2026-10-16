FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0

RUN apk add --no-cache poppler-utils \
    && addgroup -S -g 10001 pdfextract \
    && adduser -S -D -H -u 10001 -G pdfextract pdfextract \
    && mkdir -p /input \
    && touch /input/document.pdf \
    && chmod 0444 /input/document.pdf \
    && chmod 0555 /input

COPY extract-pdf /usr/local/bin/extract-pdf
RUN chmod 0555 /usr/local/bin/extract-pdf

USER 10001:10001
ENTRYPOINT ["/usr/local/bin/extract-pdf"]
