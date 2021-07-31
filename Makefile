SUFFIXES:=

GO=go
GCP_PROJECT=mkw-re
GCLOUD=gcloud

.PHONY: deploy
deploy:
	$(GCLOUD) --project "$(GCP_PROJECT)" functions deploy GenBadgeHTTP \
      --runtime go113 \
      --trigger-http \
      --allow-unauthenticated \
      --env-vars-file env.yaml
