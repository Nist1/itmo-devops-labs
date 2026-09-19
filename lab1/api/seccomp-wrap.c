#include <stdio.h>
#include <unistd.h>
#include <seccomp.h>
#include <errno.h>

int main(int argc, char *argv[]) {
    if (argc < 2) {
        fprintf(stderr, "usage: %s <path-to-binary> [args...]\n", argv[0]);
        return 1;
    }

    printf("seccomp-wrap: делаем фильтр, запрещаем unlink\n");

    scmp_filter_ctx ctx = seccomp_init(SCMP_ACT_ALLOW);
    if (ctx == NULL) {
        fprintf(stderr, "seccomp_init failed\n");
        return 1;
    }

    if (seccomp_rule_add(ctx, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(unlink), 0) < 0 ||
        seccomp_rule_add(ctx, SCMP_ACT_ERRNO(EPERM), SCMP_SYS(unlinkat), 0) < 0) {
        fprintf(stderr, "seccomp_rule_add failed\n");
        return 1;
    }

    if (seccomp_load(ctx) < 0) {
        fprintf(stderr, "seccomp_load failed\n");
        return 1;
    }
    seccomp_release(ctx);

    printf("seccomp-wrap: фильтр установлен, exec() v %s\n", argv[1]);

    execv(argv[1], &argv[1]);

    perror("execv failed");
    return 1;
  }
