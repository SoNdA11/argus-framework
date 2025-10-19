document.addEventListener("DOMContentLoaded", () => {
    // --- Seletores de Elementos ---
    const body = document.body;
    const realPowerEl = document.getElementById('real-power');
    const modifiedPowerEl = document.getElementById('modified-power');
    const heartRateEl = document.getElementById('heart-rate');
    const clientStatusEl = document.querySelector('#client-status span');
    const serverStatusEl = document.querySelector('#server-status span');
    const resistanceActiveCheck = document.getElementById('resistance-active');
    const shutdownButton = document.getElementById('shutdown-button');
    const mainModeRadios = document.querySelectorAll('input[name="main-mode"]');
    
    // Controles do Modo Boost
    const attackActiveCheck = document.getElementById('attack-active');
    const attackValueMinSlider = document.getElementById('attack-value-min');
    const attackValueMaxSlider = document.getElementById('attack-value-max');
    const sliderValueDisplay = document.getElementById('slider-value-display');
    const sliderUnitEl = document.getElementById('slider-unit');
    const attackModeRadios = document.querySelectorAll('input[name="attack-mode"]');

    // Controles do Modo Bot
    const botPowerMinSlider = document.getElementById('bot-power-min');
    const botPowerMaxSlider = document.getElementById('bot-power-max');
    const botPowerDisplay = document.getElementById('bot-power-display');
    const botCadenceMinSlider = document.getElementById('bot-cadence-min');
    const botCadenceMaxSlider = document.getElementById('bot-cadence-max');
    const botCadenceDisplay = document.getElementById('bot-cadence-display');

    // NOVOS Elementos para Conexão
    const connectionOverlay = document.getElementById('connection-overlay');
    const agentKeyInput = document.getElementById('agent-key-input');
    const connectButton = document.getElementById('connect-btn');
    const connectionStatusEl = document.getElementById('connection-status');


    // --- GRÁFICO ---
    const chartCanvas = document.getElementById('powerChart');
    const powerChart = new Chart(chartCanvas, {
        type: 'line',
        data: {
            labels: [],
            datasets: [
                {
                    label: 'POTÊNCIA_REAL (W)',
                    borderColor: 'rgba(0, 255, 204, 0.8)',
                    backgroundColor: 'rgba(0, 255, 204, 0.1)',
                    data: [], fill: true, tension: 0.4
                },
                {
                    label: 'POTÊNCIA_MODIFICADA (W)',
                    borderColor: 'rgba(255, 154, 0, 0.8)',
                    backgroundColor: 'rgba(255, 154, 0, 0.1)',
                    data: [], fill: true, tension: 0.4
                }
            ]
        },
        options: {
            responsive: true, maintainAspectRatio: false,
            plugins: { legend: { labels: { color: '#cdd6f6' } } },
            scales: {
                y: { min: 0, suggestedMax: 400, grid: { color: 'rgba(0, 255, 204, 0.1)' }, ticks: { color: '#cdd6f6' } },
                x: { grid: { display: false }, ticks: { display: false } }
            },
            animation: { duration: 0 },
        }
    });
    
    function updateChartData(realPower, modifiedPower) {
        const MAX_POINTS = 120;
        powerChart.data.labels.push('');
        powerChart.data.datasets[0].data.push(realPower);
        powerChart.data.datasets[1].data.push(modifiedPower);
        if (powerChart.data.labels.length > MAX_POINTS) {
            powerChart.data.labels.shift();
            powerChart.data.datasets.forEach(d => d.data.shift());
        }
        powerChart.update('quiet');
    }

    // --- LÓGICA DO WEBSOCKET ---
    let socket;
    function connect() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const socketURL = `${protocol}//${window.location.host}/ws`;
        socket = new WebSocket(socketURL);

        socket.onopen = () => {
            console.log("DASHBOARD CONECTADO AO SERVIDOR");
            handleMainModeChange();
            sendResistanceConfig();
        };

        socket.onmessage = (event) => {
            const data = JSON.parse(event.data);
            if (data.type === 'statusUpdate') {
                const isBotMode = document.body.classList.contains('bot-mode-active');
                realPowerEl.textContent = isBotMode ? '---' : (data.realPower || 0);
                modifiedPowerEl.textContent = data.modifiedPower;
                heartRateEl.textContent = data.heartRate || '--';
                updateChartData(isBotMode ? 0 : (data.realPower || 0), data.modifiedPower);

                // No novo modelo, 'clientConnected' agora se refere ao Agente Local.
                document.getElementById('client-status').className = data.agentConnected ? 'status-box connected' : 'status-box disconnected';
                clientStatusEl.textContent = data.agentConnected ? 'ONLINE' : 'OFFLINE';
                
                document.getElementById('server-status').className = data.appConnected ? 'status-box connected' : 'status-box disconnected';
                serverStatusEl.textContent = data.appConnected ? 'CONNECTED' : 'AWAITING_CONNECTION';
            }
        };
        socket.onclose = () => { console.log("DASHBOARD DESCONECTADO. RECONECTANDO..."); setTimeout(connect, 3000); };
        socket.onerror = (error) => { console.error("WEBSOCKET_ERROR", error); socket.close(); };
    }

    // --- FUNÇÕES DE ENVIO PARA O BACKEND ---
    function sendMainMode() {
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        const mode = document.querySelector('input[name="main-mode"]:checked').value;
        socket.send(JSON.stringify({ type: "setMainMode", payload: { mode: mode } }));
    }

    function sendBotConfig() {
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        const pMin = parseInt(botPowerMinSlider.value, 10);
        const pMax = parseInt(botPowerMaxSlider.value, 10);
        const cMin = parseInt(botCadenceMinSlider.value, 10);
        const cMax = parseInt(botCadenceMaxSlider.value, 10);
        const payload = { powerMin: pMin, powerMax: pMax, cadenceMin: cMin, cadenceMax: cMax };
        socket.send(JSON.stringify({ type: "setBotConfig", payload: payload }));
    }

    function sendPowerConfig() {
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        const mode = document.querySelector('input[name="attack-mode"]:checked').value;
        const valueMin = parseInt(attackValueMinSlider.value, 10);
        const valueMax = parseInt(attackValueMaxSlider.value, 10);
        const payload = { active: attackActiveCheck.checked, mode: mode, valueMin: valueMin, valueMax: valueMax };
        socket.send(JSON.stringify({ type: "setPowerConfig", payload: payload }));
    }
    
    function sendResistanceConfig() {
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        socket.send(JSON.stringify({ type: "setResistanceConfig", payload: { active: resistanceActiveCheck.checked } }));
    }

    // --- FUNÇÕES DE ATUALIZAÇÃO DA UI ---
    function handleMainModeChange() {
        const mode = document.querySelector('input[name="main-mode"]:checked').value;
        if (mode === 'bot') {
            body.classList.add('bot-mode-active');
            sendBotConfig();
        } else {
            body.classList.remove('bot-mode-active');
            sendPowerConfig();
        }
        sendMainMode();
    }
    
    function updateBoostSliderDisplay() {
        const mode = document.querySelector('input[name="attack-mode"]:checked').value;
        let min = parseInt(attackValueMinSlider.value, 10);
        let max = parseInt(attackValueMaxSlider.value, 10);
        if (min > max) { [min, max] = [max, min]; attackValueMinSlider.value = min; attackValueMaxSlider.value = max; }
        sliderUnitEl.textContent = (mode === 'aditivo') ? 'W' : '%';
        sliderValueDisplay.textContent = `${min} - ${max}`;
    }

    function updateBotDisplays() {
        let pMin = parseInt(botPowerMinSlider.value, 10);
        let pMax = parseInt(botPowerMaxSlider.value, 10);
        if (pMin > pMax) { [pMin, pMax] = [pMax, pMin]; botPowerMinSlider.value = pMin; botPowerMaxSlider.value = pMax; }
        botPowerDisplay.textContent = `${pMin} - ${pMax}`;

        let cMin = parseInt(botCadenceMinSlider.value, 10);
        let cMax = parseInt(botCadenceMaxSlider.value, 10);
        if (cMin > cMax) { [cMin, cMax] = [cMax, cMin]; botCadenceMinSlider.value = cMin; botCadenceMaxSlider.value = cMax; }
        botCadenceDisplay.textContent = `${cMin} - ${cMax}`;
    }

    // --- LISTENERS DE EVENTOS ---
    mainModeRadios.forEach(radio => radio.addEventListener('change', handleMainModeChange));
    
    attackActiveCheck.addEventListener('change', sendPowerConfig);
    attackValueMinSlider.addEventListener('input', updateBoostSliderDisplay);
    attackValueMaxSlider.addEventListener('input', updateBoostSliderDisplay);
    attackValueMinSlider.addEventListener('change', sendPowerConfig);
    attackValueMaxSlider.addEventListener('change', sendPowerConfig);
    attackModeRadios.forEach(radio => radio.addEventListener('change', () => { updateBoostSliderDisplay(); sendPowerConfig(); }));

    botPowerMinSlider.addEventListener('input', updateBotDisplays);
    botPowerMaxSlider.addEventListener('input', updateBotDisplays);
    botCadenceMinSlider.addEventListener('input', updateBotDisplays);
    botCadenceMaxSlider.addEventListener('input', updateBotDisplays);
    botPowerMinSlider.addEventListener('change', sendBotConfig);
    botPowerMaxSlider.addEventListener('change', sendBotConfig);
    botCadenceMinSlider.addEventListener('change', sendBotConfig);
    botCadenceMaxSlider.addEventListener('change', sendBotConfig);

    resistanceActiveCheck.addEventListener('change', sendResistanceConfig);
    shutdownButton.addEventListener('click', () => {
        if (confirm(">> Terminar a sessão do servidor MitM?")) {
            socket.send(JSON.stringify({ type: "shutdown" }));
            document.body.innerHTML = "<h1>[SESSION_TERMINATED]</h1>";
        }
    });

    // Inicialização
    updateBoostSliderDisplay();
    updateBotDisplays();
    connect();
});