document.addEventListener("DOMContentLoaded", () => {
    const usernameInput = document.getElementById('username-input');
    const agentKeyInput = document.getElementById('agent-key-input');
    const registerButton = document.getElementById('register-btn');
    const statusEl = document.getElementById('connection-status');

    registerButton.addEventListener('click', async () => {
        const username = usernameInput.value.trim();
        const agentKey = agentKeyInput.value.trim();

        if (!username || !agentKey) {
            statusEl.textContent = "Nome de usuário e Agent Key são obrigatórios.";
            statusEl.style.color = "var(--secondary-color)";
            return;
        }

        statusEl.textContent = "A processar...";
        statusEl.style.color = "var(--text-color)";

        try {
            const response = await fetch('/register', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    username: username,
                    agent_key: agentKey // O backend espera 'agent_key' (ver struct User)
                }),
            });

            if (response.status === 201) { // 201 Created
                statusEl.textContent = "Conta criada com sucesso! Redirecionando para o login...";
                statusEl.style.color = "var(--connected-color)";
                setTimeout(() => {
                    window.location.href = "/"; // Redireciona para a página de login
                }, 2000);
            } else {
                // Lê a mensagem de erro do backend
                const errorText = await response.text();
                statusEl.textContent = `Erro: ${errorText}`;
                statusEl.style.color = "var(--disconnected-color)";
            }

        } catch (error) {
            console.error("Erro ao registrar:", error);
            statusEl.textContent = "Erro de rede. Não foi possível conectar ao servidor.";
            statusEl.style.color = "var(--disconnected-color)";
        }
    });
});