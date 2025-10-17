document.addEventListener("DOMContentLoaded", () => {
    // --- Seletores de Elementos ---
    const realPowerEl = document.getElementById('real-power');
    const modifiedPowerEl = document.getElementById('modified-power');
    const heartRateEl = document.getElementById('heart-rate');
    const clientStatusEl = document.querySelector('#client-status span');
    const serverStatusEl = document.querySelector('#server-status span');
    const attackActiveCheck = document.getElementById('attack-active');
    const attackValueMinSlider = document.getElementById('attack-value-min');
    const attackValueMaxSlider = document.getElementById('attack-value-max');
    const sliderValueDisplay = document.getElementById('slider-value-display');
    const sliderUnitEl = document.getElementById('slider-unit');
    const modeRadios = document.querySelectorAll('input[name="attack-mode"]');
    const resistanceActiveCheck = document.getElementById('resistance-active');
    const shutdownButton = document.getElementById('shutdown-button');

    // --- Configuração do Gráfico com Estilo Hacker ---
    const chartCanvas = document.getElementById('powerChart');
    const powerChart = new Chart(chartCanvas, {
        type: 'line',
        data: {
            labels: [],
            datasets: [
                {
                    label: 'POTÊNCIA_REAL (W)',
                    borderColor: 'rgba(0, 255, 204, 0.8)', // Ciano
                    backgroundColor: 'rgba(0, 255, 204, 0.1)',
                    data: [],
                    fill: true,
                    tension: 0.4
                },
                {
                    label: 'POTÊNCIA_MODIFICADA (W)',
                    borderColor: 'rgba(255, 154, 0, 0.8)', // Laranja
                    backgroundColor: 'rgba(255, 154, 0, 0.1)',
                    data: [],
                    fill: true,
                    tension: 0.4
                }
            ]
        },
        options: {
            responsive: true,
            maintainAspectRatio: false,
            plugins: {
                legend: { labels: { color: '#cdd6f6' } }
            },
            scales: {
                y: {
                    min: 0,
                    suggestedMax: 400,
                    grid: { color: 'rgba(0, 255, 204, 0.1)' },
                    ticks: { color: '#cdd6f6' }
                },
                x: {
                    grid: { display: false },
                    ticks: { display: false }
                }
            },
            animation: { duration: 0 },
        }
    });

    function updateChart(realPower, modifiedPower) {
        const MAX_POINTS = 50;
        powerChart.data.labels.push(''); // Não precisamos de labels de tempo
        powerChart.data.datasets[0].data.push(realPower);
        powerChart.data.datasets[1].data.push(modifiedPower);
        if (powerChart.data.labels.length > MAX_POINTS) {
            powerChart.data.labels.shift();
            powerChart.data.datasets.forEach(d => d.data.shift());
        }
        powerChart.update('quiet');
    }

    // --- Lógica do WebSocket (sem alterações na funcionalidade) ---
    let socket;
    function connect() {
        socket = new WebSocket(`ws://${window.location.host}/ws`);
        socket.onopen = () => { console.log("CONNECTION_ESTABLISHED"); sendPowerConfig(); sendResistanceConfig(); };
        socket.onmessage = (event) => {
            const data = JSON.parse(event.data);
            if (data.type === 'statusUpdate') {
                realPowerEl.textContent = data.realPower;
                modifiedPowerEl.textContent = data.modifiedPower;
                heartRateEl.textContent = data.heartRate;

                const clientStatusDiv = document.getElementById('client-status');
                clientStatusDiv.className = data.clientConnected ? 'status-box connected' : 'status-box disconnected';
                clientStatusEl.textContent = data.clientConnected ? 'ONLINE' : 'OFFLINE';
                
                const serverStatusDiv = document.getElementById('server-status');
                serverStatusDiv.className = data.appConnected ? 'status-box connected' : 'status-box disconnected';
                serverStatusEl.textContent = data.appConnected ? 'CONNECTED' : 'AWAITING_CONNECTION';
                
                updateChart(data.realPower, data.modifiedPower);
            }
        };
        socket.onclose = () => { console.log("CONNECTION_LOST. RECONNECTING..."); setTimeout(connect, 3000); };
        socket.onerror = (error) => { console.error("WEBSOCKET_ERROR", error); socket.close(); };
    }

    // MUDANÇA: sendPowerConfig agora envia um range
    function sendPowerConfig() {
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        const mode = document.querySelector('input[name="attack-mode"]:checked').value;
        const valueMin = parseInt(attackValueMinSlider.value, 10);
        const valueMax = parseInt(attackValueMaxSlider.value, 10);

        const config = {
            type: "setPowerConfig",
            payload: {
                active: attackActiveCheck.checked,
                mode: mode,
                valueMin: valueMin,
                valueMax: valueMax,
            }
        };
        socket.send(JSON.stringify(config));
    }
    
    function sendResistanceConfig() {
        if (!socket || socket.readyState !== WebSocket.OPEN) return;
        const config = { type: "setResistanceConfig", payload: { active: resistanceActiveCheck.checked } };
        socket.send(JSON.stringify(config));
    }

    // MUDANÇA: Listener agora controla os dois sliders
    function updateSliderDisplay() {
        const mode = document.querySelector('input[name="attack-mode"]:checked').value;
        let min = parseInt(attackValueMinSlider.value, 10);
        let max = parseInt(attackValueMaxSlider.value, 10);

        // Garante que min não seja maior que max
        if (min > max) {
            [min, max] = [max, min]; // Inverte os valores
            attackValueMinSlider.value = min;
            attackValueMaxSlider.value = max;
        }

        sliderUnitEl.textContent = (mode === 'aditivo') ? 'W' : '%';
        sliderValueDisplay.textContent = `${min} - ${max}`;
    }

    // --- Listeners de Eventos (ATUALIZADO) ---
    attackActiveCheck.addEventListener('change', sendPowerConfig);
    
    attackValueMinSlider.addEventListener('input', updateSliderDisplay);
    attackValueMaxSlider.addEventListener('input', updateSliderDisplay);
    
    attackValueMinSlider.addEventListener('change', sendPowerConfig);
    attackValueMaxSlider.addEventListener('change', sendPowerConfig);

    modeRadios.forEach(radio => radio.addEventListener('change', () => {
        updateSliderDisplay();
        sendPowerConfig();
    }));

    resistanceActiveCheck.addEventListener('change', sendResistanceConfig);

    shutdownButton.addEventListener('click', () => {
        if (confirm(">> Terminar a sessão do servidor MitM?")) {
            socket.send(JSON.stringify({ type: "shutdown" }));
            document.body.innerHTML = "<h1>[SESSION_TERMINATED]</h1>";
        }
    });
    
    // Inicializa a UI e a conexão
    updateSliderDisplay();
    connect();
});

// Antes de trocar