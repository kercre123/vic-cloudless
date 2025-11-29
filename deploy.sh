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

echo "Would you like to setup a swapfile? This improves the performance of cloudless by quite a bit. (Takes ~200mb in /data)"
echo -n "(y/n): "
read SWAPFILE

if [[ ${SWAPFILE} == "y" ]]; then
    echo "Will use swapfile"
else 
	echo "No swapfile"
fi

ssh -i ssh_root_key root@$1 "systemctl stop anki-robot.target && mount -o rw,remount / && rm -rf /anki/data/assets/cozmo_resources/cloudless && mkdir -p /anki/data/assets/cozmo_resources/cloudless"
scp -i ssh_root_key build/vic-cloud root@$1:/anki/bin/
scp -i ssh_root_key build/lib* root@$1:/anki/lib/
scp -i ssh_root_key extra/cloud.sudoers root@$1:/etc/sudoers.d/cloud
scp -i ssh_root_key extra/setfreq root@$1:/usr/sbin/
ssh -i ssh_root_key root@$1 "sed -i \"s/Nice=\-2/Nice=3/g\" /usr/lib/systemd/system/vic-anim.service"
rsync -e 'ssh -i ssh_root_key' -avr build/en-US root@$1:/anki/data/assets/cozmo_resources/cloudless/

if [[ ${SWAPFILE} == "y" ]]; then
	if [[ `ssh -i ssh_root_key root@$1 "swapon -s"` == "" ]]; then
	   	#ssh -i ssh_root_key root@$1 "dd if=/dev/zero of=/data/swapfile bs=1024 count=200576"
		ssh -i ssh_root_key root@$1 "chmod 600 /data/swapfile"
		ssh -i ssh_root_key root@$1 "mkswap /data/swapfile"
		ssh -i ssh_root_key root@$1 "swapon /data/swapfile"
		ssh -i ssh_root_key root@$1 "echo "/data/swapfile     none    swap    sw    0   0" >> /etc/fstab"
	fi
fi

ssh -i ssh_root_key root@$1 "chmod +rwx /usr/sbin/setfreq && systemctl daemon-reload && sudo -k && systemctl start anki-robot.target"
