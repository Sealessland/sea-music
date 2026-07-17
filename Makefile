.PHONY: bootstrap verify verify-observability fault-drill loadtest benchmark final-verify

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
