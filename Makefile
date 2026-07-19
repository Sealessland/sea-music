.PHONY: bootstrap proto-lint proto-generate verify verify-observability fault-drill loadtest benchmark final-verify

proto-lint:
	buf format --diff --exit-code
	buf lint

proto-generate:
	buf generate

bootstrap:
	./scripts/bootstrap.sh

verify:
	./scripts/verify.sh

verify-observability:
	./scripts/verify-observability.sh

fault-drill:
	./scripts/fault-drill.sh

loadtest:
	./scripts/loadtest.sh

benchmark:
	./scripts/benchmark.sh

final-verify:
	./scripts/final-verify.sh
