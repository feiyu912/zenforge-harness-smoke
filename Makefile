.PHONY: run run-http run-cli test build clean

run: run-http

# `make run` starts the local HTTP test server on 127.0.0.1:8088 with the
# offline scripted model. This is the surface the zenforge-testui UI talks
# to. Pass MAKE_ADDR=0.0.0.0:9000 to change the bind, MAKE_MODEL=minimax to
# use the real provider.
run-http:
	go run . --http --addr $(or $(MAKE_ADDR),127.0.0.1:8088) $(if $(MAKE_MODEL),--model $(MAKE_MODEL),)

# `make run-cli Q="what is the weather"` runs a single CLI query.
run-cli:
	go run . -q "$(Q)"

test:
	go test ./...

build:
	go build -o zenforge-harness-smoke .

clean:
	rm -f zenforge-harness-smoke
