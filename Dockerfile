FROM golang:1.20-alpine as dev

# This is needed for some go mod commands
RUN apk --no-cache add git
# This is needed for tests
RUN apk --no-cache add make gcc libc-dev

# Set the working directory according to the package name
RUN mkdir -p /go/src/github.com/agschwender/go-local
WORKDIR /go/src/github.com/agschwender/go-local


ENV GOPRIVATE=github.com/agschwender/errcat-go

# Copy credentials
COPY .netrc /root/.netrc

# Copy dependency configuration and download. This will allow caching
# of these steps unless the configuration is changed.
COPY go.mod go.sum .
RUN go mod download

# Copy everything else over
COPY . .

# Install all commands
RUN go install -v -buildvcs=false ./cmd/...

CMD ["/go/bin/example"]

# Use a smaller image when running the application. Can probably get
# away with scratch here, but find it convenient to be able to jump on
# the container.
FROM alpine

# Copy over executable
COPY --from=dev /go/bin/example /go/bin/example

WORKDIR /go/bin

CMD ["/go/bin/example"]
