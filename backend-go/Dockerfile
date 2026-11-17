ARG GO_IMAGE=golang:1.25.1-alpine
ARG ALPINE_IMAGE=alpine:3.22
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY backend-go/go.mod backend-go/go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN go mod download
COPY backend-go/ ./
RUN mkdir -p /app/frontend/brand /app/backend-go/config/prompts
COPY backend-go/config/changelog.json /app/backend-go/config/changelog.json
COPY backend-go/config/public/ /app/backend-go/config/public/
COPY backend-go/config/model-catalog.json /app/backend-go/config/model-catalog.json
COPY frontend/brand/ /app/frontend/brand/
COPY backend-go/config/publication.json /app/backend-go/config/publication.json
COPY backend-go/config/entities.json /app/backend-go/config/entities.json
COPY backend-go/config/categories.json /app/backend-go/config/categories.json
COPY backend-go/config/vocabulary.json /app/backend-go/config/vocabulary.json
COPY backend-go/config/selection.json /app/backend-go/config/selection.json
COPY backend-go/config/reporting.json /app/backend-go/config/reporting.json
COPY backend-go/config/topics.json /app/backend-go/config/topics.json
COPY backend-go/config/prompts/ /app/backend-go/config/prompts/
RUN CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/myhot-api ./cmd/api \
 && CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/myhot-worker ./cmd/worker \
 && CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/eval-relations ./cmd/eval-relations \
 && CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/eval-selection ./cmd/eval-selection \
 && CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/eval-story-digests ./cmd/eval-story-digests

FROM ${ALPINE_IMAGE}
ARG ALPINE_MIRROR=dl-cdn.alpinelinux.org
RUN sed -i "s|dl-cdn.alpinelinux.org|${ALPINE_MIRROR}|g" /etc/apk/repositories \
 && apk add --no-cache ca-certificates tzdata postgresql17-client tar
WORKDIR /app
RUN mkdir -p /app/frontend/brand /app/backend-go/config/prompts /data && chown 1000:1000 /data
COPY --from=build /app/backend-go/config/changelog.json /app/backend-go/config/changelog.json
COPY --from=build /app/backend-go/config/public/ /app/backend-go/config/public/
COPY --from=build /app/backend-go/config/model-catalog.json /app/backend-go/config/model-catalog.json
COPY --from=build /app/backend-go/config/publication.json /app/backend-go/config/publication.json
COPY --from=build /app/frontend/brand/ /app/frontend/brand/
COPY --from=build /app/backend-go/config/entities.json /app/backend-go/config/entities.json
COPY --from=build /app/backend-go/config/categories.json /app/backend-go/config/categories.json
COPY --from=build /app/backend-go/config/vocabulary.json /app/backend-go/config/vocabulary.json
COPY --from=build /app/backend-go/config/selection.json /app/backend-go/config/selection.json
COPY --from=build /app/backend-go/config/reporting.json /app/backend-go/config/reporting.json
COPY --from=build /app/backend-go/config/topics.json /app/backend-go/config/topics.json
COPY --from=build /app/backend-go/config/prompts/ /app/backend-go/config/prompts/
COPY --from=build /out/myhot-api /app/myhot-api
COPY --from=build /out/myhot-worker /app/myhot-worker
COPY --from=build /out/eval-relations /app/eval-relations
COPY --from=build /out/eval-selection /app/eval-selection
COPY --from=build /out/eval-story-digests /app/eval-story-digests
USER 1000:1000
EXPOSE 3002
CMD ["/app/myhot-api"]
