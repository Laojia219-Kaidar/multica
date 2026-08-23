#define _GNU_SOURCE

#include <errno.h>
#include <fcntl.h>
#include <linux/landlock.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <unistd.h>

#ifndef __NR_landlock_create_ruleset
#error "Landlock syscall numbers are unavailable on this platform"
#endif

static int landlock_create_ruleset(const void *attr, size_t size, uint32_t flags) {
    return (int)syscall(__NR_landlock_create_ruleset, attr, size, flags);
}

static int landlock_add_rule(int ruleset_fd, enum landlock_rule_type type,
                             const void *attr, uint32_t flags) {
    return (int)syscall(__NR_landlock_add_rule, ruleset_fd, type, attr, flags);
}

static int landlock_restrict_self(int ruleset_fd, uint32_t flags) {
    return (int)syscall(__NR_landlock_restrict_self, ruleset_fd, flags);
}

static void usage(const char *name) {
    fprintf(stderr, "usage: %s --write ABSOLUTE_PATH [--write ABSOLUTE_PATH ...] -- COMMAND [ARG ...]\n", name);
}

static void fail_errno(const char *operation) {
    fprintf(stderr, "hivecrew-landlock-exec: %s: %s\n", operation, strerror(errno));
    exit(1);
}

int main(int argc, char **argv) {
    if (argc < 5) {
        usage(argv[0]);
        return 2;
    }

    char **write_paths = calloc((size_t)argc, sizeof(char *));
    if (write_paths == NULL) {
        fail_errno("allocate write path list");
    }

    int write_count = 0;
    int command_index = -1;
    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--write") == 0) {
            if (i + 1 >= argc) {
                usage(argv[0]);
                return 2;
            }
            write_paths[write_count++] = argv[++i];
            continue;
        }
        if (strcmp(argv[i], "--") == 0) {
            command_index = i + 1;
            break;
        }
        usage(argv[0]);
        return 2;
    }

    if (write_count == 0 || command_index < 0 || command_index >= argc) {
        usage(argv[0]);
        return 2;
    }

    int abi = landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION);
    if (abi < 1) {
        fail_errno("query Landlock ABI");
    }

    uint64_t handled = LANDLOCK_ACCESS_FS_WRITE_FILE |
                       LANDLOCK_ACCESS_FS_REMOVE_DIR |
                       LANDLOCK_ACCESS_FS_REMOVE_FILE |
                       LANDLOCK_ACCESS_FS_MAKE_CHAR |
                       LANDLOCK_ACCESS_FS_MAKE_DIR |
                       LANDLOCK_ACCESS_FS_MAKE_REG |
                       LANDLOCK_ACCESS_FS_MAKE_SOCK |
                       LANDLOCK_ACCESS_FS_MAKE_FIFO |
                       LANDLOCK_ACCESS_FS_MAKE_BLOCK |
                       LANDLOCK_ACCESS_FS_MAKE_SYM;
    if (abi >= 2) {
        handled |= LANDLOCK_ACCESS_FS_REFER;
    }
    if (abi >= 3) {
        handled |= LANDLOCK_ACCESS_FS_TRUNCATE;
    }

    const struct landlock_ruleset_attr ruleset_attr = {
        .handled_access_fs = handled,
    };
    int ruleset_fd = landlock_create_ruleset(&ruleset_attr, sizeof(ruleset_attr), 0);
    if (ruleset_fd < 0) {
        fail_errno("create ruleset");
    }

    for (int i = 0; i < write_count; i++) {
        if (write_paths[i][0] != '/') {
            fprintf(stderr, "hivecrew-landlock-exec: write path must be absolute: %s\n", write_paths[i]);
            return 2;
        }
        int path_fd = open(write_paths[i], O_PATH | O_CLOEXEC);
        if (path_fd < 0) {
            fail_errno("open allowed write path");
        }
        const struct landlock_path_beneath_attr path_attr = {
            .allowed_access = handled,
            .parent_fd = path_fd,
        };
        if (landlock_add_rule(ruleset_fd, LANDLOCK_RULE_PATH_BENEATH, &path_attr, 0) < 0) {
            fail_errno("add allowed write path");
        }
        close(path_fd);
    }

    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) < 0) {
        fail_errno("set no_new_privs");
    }
    if (landlock_restrict_self(ruleset_fd, 0) < 0) {
        fail_errno("restrict process");
    }
    close(ruleset_fd);

    if (setenv("SANDBOX", "landlock", 1) < 0) {
        fail_errno("set SANDBOX marker");
    }
    unsetenv("QWEN_SANDBOX");

    execvp(argv[command_index], &argv[command_index]);
    fail_errno("execute command");
    return 1;
}
