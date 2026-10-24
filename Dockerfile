FROM golang:latest AS build

WORKDIR /workspace
COPY . .
RUN go build -o app .

FROM gcr.io/distroless/cc-debian13:latest

COPY --from=build /workspace/app /

ENTRYPOINT [ "/app" ]
