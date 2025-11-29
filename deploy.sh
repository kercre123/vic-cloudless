#!/bin/bash

set -e

if [[ $1 == "" ]]; then
	echo "provide ip please"
	exit 1
fi

if [[ ! -f ssh_root_key ]]; then
	wget modder.my.to/ssh_root_key
fi

chmod 600 ssh_root_key

ssh -i ssh_root_key root@$1 "systemctl stop anki-robot.target && mount -o rw,remount / && rm -rf /anki/data/assets/cozmo_resources/cloudless && mkdir -p /anki/data/assets/cozmo_resources/cloudless"
scp -i ssh_root_key build/vic-cloud root@$1:/anki/bin/
scp -i ssh_root_key build/lib* root@$1:/anki/lib/
scp -i ssh_root_key extra/cloud.sudoers root@$1:/etc/sudoers.d/cloud
scp -i ssh_root_key extra/setfreq root@$1:/usr/sbin/
ssh -i ssh_root_key root@$1 "sed -i \"s/Nice=\-2/Nice=3/g\" /usr/lib/systemd/system/vic-anim.service"
rsync -e 'ssh -i ssh_root_key' -avr build/en-US root@$1:/anki/data/assets/cozmo_resources/cloudless/
ssh -i ssh_root_key root@$1 "chmod +rwx /usr/sbin/setfreq && systemctl daemon-reload && sudo -k && systemctl start anki-robot.target"
