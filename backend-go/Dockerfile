FROM golang:1.25.1-alpine AS build
WORKDIR /src
COPY backend-go/go.mod backend-go/go.sum ./
RUN go mod download
COPY backend-go/ ./
RUN mkdir -p /app/shared/site /app/shared/site/brand /app/shared/industry /app/shared/industry/prompts
COPY shared/site/changelog.json /app/shared/site/changelog.json
COPY shared/site/public/ /app/shared/site/public/
COPY shared/site/model-catalog.json /app/shared/site/model-catalog.json
COPY shared/site/publication.json /app/shared/site/publication.json
COPY shared/site/brand/ /app/shared/site/brand/
COPY shared/industry/entities.json /app/shared/industry/entities.json
COPY shared/industry/categories.json /app/shared/industry/categories.json
COPY shared/industry/vocabulary.json /app/shared/industry/vocabulary.json
COPY shared/industry/selection.json /app/shared/industry/selection.json
COPY shared/industry/reporting.json /app/shared/industry/reporting.json
COPY shared/industry/topics.json /app/shared/industry/topics.json
COPY shared/industry/prompts/translate-body.md /app/shared/industry/prompts/translate-body.md
COPY shared/industry/prompts/translate-post.md /app/shared/industry/prompts/translate-post.md
COPY shared/industry/prompts/report-period.md /app/shared/industry/prompts/report-period.md
COPY shared/industry/prompts/report-period-sections.md /app/shared/industry/prompts/report-period-sections.md
COPY shared/industry/prompts/report-period-no-sections.md /app/shared/industry/prompts/report-period-no-sections.md
RUN CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/myhot-api ./cmd/api \
 && CGO_ENABLED=0 go build -tags nodynamic -trimpath -ldflags="-s -w" -o /out/myhot-worker ./cmd/worker

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata postgresql17-client tar
WORKDIR /app
RUN mkdir -p /app/shared/site /app/shared/site/brand /app/shared/industry /app/shared/industry/prompts /data && chown 1000:1000 /data
COPY --from=build /app/shared/site/changelog.json /app/shared/site/changelog.json
COPY --from=build /app/shared/site/public/ /app/shared/site/public/
COPY --from=build /app/shared/site/model-catalog.json /app/shared/site/model-catalog.json
COPY --from=build /app/shared/site/publication.json /app/shared/site/publication.json
COPY --from=build /app/shared/site/brand/ /app/shared/site/brand/
COPY --from=build /app/shared/industry/entities.json /app/shared/industry/entities.json
COPY --from=build /app/shared/industry/categories.json /app/shared/industry/categories.json
COPY --from=build /app/shared/industry/vocabulary.json /app/shared/industry/vocabulary.json
COPY --from=build /app/shared/industry/selection.json /app/shared/industry/selection.json
COPY --from=build /app/shared/industry/reporting.json /app/shared/industry/reporting.json
COPY --from=build /app/shared/industry/topics.json /app/shared/industry/topics.json
COPY --from=build /app/shared/industry/prompts/translate-body.md /app/shared/industry/prompts/translate-body.md
COPY --from=build /app/shared/industry/prompts/translate-post.md /app/shared/industry/prompts/translate-post.md
COPY --from=build /app/shared/industry/prompts/report-period.md /app/shared/industry/prompts/report-period.md
COPY --from=build /app/shared/industry/prompts/report-period-sections.md /app/shared/industry/prompts/report-period-sections.md
COPY --from=build /app/shared/industry/prompts/report-period-no-sections.md /app/shared/industry/prompts/report-period-no-sections.md
COPY --from=build /out/myhot-api /app/myhot-api
COPY --from=build /out/myhot-worker /app/myhot-worker
USER 1000:1000
EXPOSE 3002
CMD ["/app/myhot-api"]
