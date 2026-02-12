/**
 * @file network_test.cpp
 * @brief 网络访问测试
 * 
 * 预期行为:
 * - Seccomp 白名单不包含 socket 系统调用
 * - 调用 socket() 时触发 SIGSYS (Signal 31)
 * 
 * 预期结果: RE (Runtime Error)
 * 错误信息可能包含: "Bad system call" 或 "Signal 31"
 */
#include <sys/socket.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <unistd.h>

int main() {
    // 尝试创建 TCP socket
    int sockfd = socket(AF_INET, SOCK_STREAM, 0);
    if (sockfd < 0) {
        return 1;  // 如果 seccomp 没生效，这里会返回 -1 (EPERM)
    }
    
    // 尝试连接到外部服务器
    struct sockaddr_in server_addr;
    server_addr.sin_family = AF_INET;
    server_addr.sin_port = htons(80);
    inet_pton(AF_INET, "8.8.8.8", &server_addr.sin_addr);
    
    connect(sockfd, (struct sockaddr*)&server_addr, sizeof(server_addr));
    
    close(sockfd);
    return 0;
}
