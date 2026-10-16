# v2hide.ko 构建
# 目标机需要对应内核的 headers: apt install linux-headers-$(uname -r) / dnf install kernel-devel
obj-m += v2hide.o
ccflags-y += -Wno-unused-variable

KDIR ?= /lib/modules/$(shell uname -r)/build
PWD  := $(shell pwd)

all:
	$(MAKE) -C $(KDIR) M=$(PWD) modules

clean:
	$(MAKE) -C $(KDIR) M=$(PWD) clean

install: all
	install -m 600 v2hide.ko /etc/v2nb/v2hide.ko

# 交叉编译（为目标机器构建）：
# make KDIR=/path/to/target-kernel-source ARCH=x86_64 CROSS_COMPILE=x86_64-linux-gnu-

.PHONY: all clean install
