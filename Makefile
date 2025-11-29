#.PHONY: en-US voskopus vic-cloud vic-gateway

#https://alphacephei.com/vosk/models/vosk-model-small-en-us-0.15.zip

MODEL_ZIP = vosk-model-small-en-us-0.15.zip
MODEL_URL = https://github.com/kercre123/vosk-models/raw/refs/heads/main/$(MODEL_ZIP)
MODEL_DIR = build/en-US/model
MODEL_UNZIPPED = build/en-US/model/.unzipped

INTENT_JSON = build/en-US/en-US.json
INTENT_URL = https://github.com/kercre123/wire-pod/raw/refs/heads/main/chipper/intent-data/en-US.json

all: $(MODEL_UNZIPPED) $(INTENT_JSON) vic-cloud

$(MODEL_UNZIPPED):
	mkdir -p build/en-US
	wget -nc $(MODEL_URL)
	unzip vosk-model-small-en-us-0.15.zip
	mv -n vosk-model-small-en-us-0.15 $(MODEL_DIR)
	rm -f $(MODEL_ZIP)
	touch $(MODEL_UNZIPPED)

$(INTENT_JSON):
	mkdir -p build/en-US
	wget -nc -O $(INTENT_JSON) $(INTENT_URL)

gettoolchain:
	./get-deps.sh

voskopusbuild:
	./build-voskopus.sh

go_deps:
	echo `go version` && cd $(PWD) && go mod download

vic-cloud: gettoolchain voskopusbuild go_deps
	CGO_ENABLED=1 GOARM=7 GOARCH=arm \
	CC=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang \
	CXX=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang++ \
	PKG_CONFIG_PATH="$(PWD)/voskopus/built/armel/lib/pkgconfig" \
	CGO_CFLAGS="-Wno-implicit-function-declaration -I$(PWD)/voskopus/built/armel/include -I$(PWD)/voskopus/built/armel/include/opus" \
	CGO_CXXFLAGS="-stdlib=libc++ -std=c++11" \
	CGO_LDFLAGS="-L$(PWD)/voskopus/built/armel/lib -L$(PWD)/armlibs/lib/arm-linux-gnueabi/android -lpthread" \
	go build \
	-tags nolibopusfile,vicos \
	-ldflags '-w -s -linkmode internal -extldflags "-static" -r /anki/lib' \
	-o build/vic-cloud \
	cloud/*

	#upx build/vic-cloud


#vic-gateway: go_deps
#	CGO_ENABLED=1 GOARM=7 GOARCH=arm CC=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang CXX=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang++ PKG_CONFIG_PATH="$(PWD)/voskopus/lib/pkgconfig" CGO_CFLAGS="-I$(PWD)/voskopus/include -I$(PWD)/voskopus/include/opus -I$(PWD)/voskopus/include/ogg" CGO_CXXFLAGS="-stdlib=libc++ -std=c++11" CGO_LDFLAGS="-L$(PWD)/voskopus/lib -L$(PWD)/armlibs/lib/arm-linux-gnueabi/android" go build -tags nolibopusfile,vicos -ldflags '-w -s -linkmode internal -extldflags "-static" -r /anki/lib' -o build/vic-gateway gateway/*.go

#	#upx build/vic-gateway

