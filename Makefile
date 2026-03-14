IMAGE ?= drengskapr/external-dns-webhook-ngcloud
TAG   ?= latest

build:
	go build -o external-dns-webhook-ngcloud .

test:
	go test ./...

docker-build:
	docker build -t $(IMAGE):$(TAG) .
