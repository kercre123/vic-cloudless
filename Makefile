#.PHONY: en-US voskopus vic-cloud vic-gateway

#https://alphacephei.com/vosk/models/vosk-model-small-en-us-0.15.zip

SHERPA_URL = https://github.com/kercre123/vic-cloudless/releases/download/v0.0.1/sherpa.tar.gz
SHERPA_UNZIPPED = build/sherpa/.unzipped

INTENT_JSON = build/en-US/en-US.json
INTENT_URL = https://github.com/kercre123/wire-pod/raw/refs/heads/main/chipper/intent-data/en-US.json

all: $(SHERPA_UNZIPPED) $(INTENT_JSON) vic-cloud

$(INTENT_JSON):
	mkdir -p build/en-US
	wget -q --show-progress -O $(INTENT_JSON) $(INTENT_URL)

gettoolchain:
	./get-deps.sh

$(SHERPA_UNZIPPED):
	mkdir -p build/
	wget -q --show-progress $(SHERPA_URL)
	tar -zxvf sherpa.tar.gz
	mv sherpa build/
	rm -f sherpa.tar.gz
	touch $(SHERPA_UNZIPPED)

opusbuild:
	./build-voskopus.sh

go_deps:
	echo `go version` && cd $(PWD) && go mod download

vic-cloud: gettoolchain opusbuild go_deps
	CGO_ENABLED=1 GOARM=7 GOARCH=arm \
	CC=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang \
	CXX=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang++ \
	PKG_CONFIG_PATH="$(PWD)/voskopus/built/armel/lib/pkgconfig" \
	CGO_CFLAGS="-Wno-implicit-function-declaration -I$(PWD)/voskopus/built/armel/include -I$(PWD)/voskopus/built/armel/include/opus" \
	CGO_CXXFLAGS="-stdlib=libc++ -std=c++11" \
	CGO_LDFLAGS="-L$(PWD)/voskopus/built/armel/lib -L$(PWD)/armlibs/lib/arm-linux-gnueabi/android" \
	go build \
	-tags nolibopusfile,vicos \
	-ldflags '-w -s' \
	-o build/vic-cloud \
	cloud/*

	#upx build/vic-cloud


#vic-gateway: go_deps
#	CGO_ENABLED=1 GOARM=7 GOARCH=arm CC=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang CXX=${HOME}/.anki/vicos-sdk/dist/5.3.0-r07/prebuilt/bin/arm-oe-linux-gnueabi-clang++ PKG_CONFIG_PATH="$(PWD)/voskopus/lib/pkgconfig" CGO_CFLAGS="-I$(PWD)/voskopus/include -I$(PWD)/voskopus/include/opus -I$(PWD)/voskopus/include/ogg" CGO_CXXFLAGS="-stdlib=libc++ -std=c++11" CGO_LDFLAGS="-L$(PWD)/voskopus/lib -L$(PWD)/armlibs/lib/arm-linux-gnueabi/android" go build -tags nolibopusfile,vicos -ldflags '-w -s -linkmode internal -extldflags "-static" -r /anki/lib' -o build/vic-gateway gateway/*.go

#	#upx build/vic-gateway

