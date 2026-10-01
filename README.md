# comecozend

Apps para dArkOS no MiniLoong:

- `leaf-mlp1-poc/` — navegador de homebrew do itch.io / hub RetroAchievements (v96)
- `rom-hacks/` — ROM hacks do RetroAchievements aplicados às ROMs do aparelho (v34)

Cada pasta é um módulo Go independente; veja o README de cada uma.

## Instalar no aparelho

Gere os pacotes (ou use os de um release) e copie-os junto com o script:

```sh
./release.sh 36 98          # dist/rom-hacks-v36.tar.gz, dist/leaf-mlp1-poc-v98.tar.gz
scp dist/rom-hacks-v36.tar.gz dist/leaf-mlp1-poc-v98.tar.gz install-on-device.sh ark@<aparelho>:~/
ssh ark@<aparelho> ./install-on-device.sh 36 98
```

Use `-` para pular um dos apps (`./install-on-device.sh 36 -`). Cada app é
compilado numa pasta temporária e só substitui a versão instalada depois de
compilar; se algo falhar, a anterior continua funcionando, e o
EmulationStation é religado em qualquer caso.
