# ROM Hacks — v1

## v35 — correções de bugs

- PS1 com faixas de áudio: cada faixa vai para `hacks/` e o `.cue` aponta
  cada `FILE` para a sua (antes todas apontavam para a faixa de dados e o
  jogo ficava sem música). "Delete this hack" apaga as faixas também.
- Disco cujo hash não pode ser calculado não fica mais instalado sem
  verificação.
- Trocar de página com L/R durante o preparo não faz mais o A instalar o
  hack da página anterior, e o arquivo baixado da página anterior é
  liberado.
- Patch corrompido dá erro em vez de fechar o app.
- Patches grandes (PS1, ~80 MB) baixam em Wi-Fi lento: o limite de 2 min
  por download virou "60 s sem receber nada". Download maior que o limite
  é recusado em vez de truncado.
- "Delete this hack" só apaga arquivos que o próprio app instalou; hacks
  que você já tinha em `hacks/` ficam intactos.
- Mensagem de erro útil ("this patch needs ...") quando só há uma cópia
  do jogo base.

Lista os ROM hacks do RetroAchievements que dá para montar com as ROMs que
já estão neste aparelho, baixa o patch do repositório do próprio RA, aplica
localmente e instala o resultado em `/roms`.

Só entram hacks **com conjunto de conquistas próprio**. Dois filtros:
um patch cujo set no RA tem zero conquistas não aparece; e um patch
arquivado sob o **ID do próprio jogo base** também não, porque não é hack —
é patch de compatibilidade (o RAPatches guarda quatro deles em
`NES/Hacks/Ninja Gaiden/`, todos com o ID 1859, que é o Ninja Gaiden
original). Um patch cujo set no RA tem
zero conquistas não aparece na lista: o objetivo é jogar valendo alguma coisa,
e o repositório hospeda muito patch sem set.

A conferência final aceita **qualquer** hash que o RA reconheça para aquele
set, não só o que o nome do arquivo sugeria. Um arquivo costuma trazer várias
versões do hack, e adivinhar qual `.bps` corresponde a qual hash pelo nome
falha sempre que o autor nomeou diferente do site. Se o primeiro patch não
produzir algo reconhecido, os outros do mesmo arquivo são tentados.

Nenhuma ROM pronta é baixada: o aparelho entrega o jogo base, a rede entrega
só o diff. E nada é instalado se o arquivo gerado não bater com o hash que o
RetroAchievements publica — um hack que roda mas não dá conquista é
exatamente o fracasso que este app existe para evitar.

## Como o app sabe o que está instalado

Pelo **ID do set**, anotado em `installed.json` na hora de instalar — nunca
por nome. O RetroAchievements chama o set de "Pokémon Emerald Rogue V2" e o
arquivo que ele espera é "Pokemon Emerald - Emerald Rogue (v2.0).gba": dois
nomes para a mesma coisa que normalização nenhuma junta. Registro que some do
cartão é removido no próximo scan.

## Um set, dois jogos base

"Pokémon Regulation Red | Regulation Blue" é um set só, com um ID só, e o
repositório o arquiva tanto em `Pokemon Red` quanto em `Pokemon Blue`. Quem
tem os dois cartuchos via o mesmo hack duas vezes na lista. Agora aparece uma
linha só, dizendo "hack of Pokémon Red Version or Pokémon Blue Version", e as
duas ROMs continuam válidas para instalar.

## Várias cópias do mesmo jogo

Tendo duas cópias na pasta — um `Pokemon - Crystal Version (UE) (V1.0)` e um
`(USA, Europe) (Rev 1)`, por exemplo — o app usa o checksum do patch para
escolher a certa. Quando o patch não declara checksum nenhum (um IPS avulso
sem readme), ele **tenta cada cópia** até uma produzir um arquivo que o
RetroAchievements reconheça, e a página passa a nomear a que funcionou.
Disco fica de fora dessa repetição: cada tentativa significaria extrair
600 MB de novo.

Também vale o caso do header: o checksum sem header do iNES ou do copier é
indexado junto com o do arquivo inteiro, porque autores de patch discordam
sobre incluí-lo e um dump pode ser a ROM certa sem bater no checksum cheio.

## Onde o hack é salvo

Dentro da pasta do sistema, numa subpasta `hacks`:

    /roms/gba/Fire Emblem - The Sacred Stones (USA).gba    ← seu original, intacto
    /roms/gba/hacks/FE8 - The Hag in White (v2.5).gba      ← o hack
    /roms/gba/hacks/images/...                             ← as imagens do RA

A ROM original nunca é modificada nem movida.

## Primeiro uso

```sh
./build.sh                                   # compila (no aparelho)
./install-tool.sh                            # registra em Tools no ES (RA Hack.sh)
./bin/rom-hacks-arm64 --ra-login nicefrog SUA_CHAVE
./bin/rom-hacks-arm64 --refresh              # varre /roms e monta a lista
./bin/rom-hacks-arm64 --list
```

A chave fica em https://retroachievements.org/controlpanel.php.

Config, caches e o catálogo montado ficam em `~/.local/share/rom-hacks/`,
fora da pasta do app de propósito: atualizar é apagar a pasta e descompactar
a nova, e a chave do RA e as impressões digitais das ROMs sobrevivem a isso.

## Comandos

| Comando | O que faz |
| --- | --- |
| `--ra-login <user> <key>` | salva a credencial do RetroAchievements |
| `--scan` | fingerprinta `/roms` e guarda o resultado |
| `--refresh` | rescan + índice de patches + reconstrói a lista |
| `--list` | imprime os hacks disponíveis para suas ROMs |
| `--bases [texto]` | quais ROMs suas foram identificadas, com quantos hacks cada uma, e quais arquivos o RA **não** reconheceu |
| `--install <game id>` | baixa, aplica e instala um hack |
| *(sem argumento)* | abre a interface no aparelho |

A CLI existe porque digitar uma chave de API no teclado de tela é sofrimento,
e porque dá para inspecionar o pipeline inteiro por SSH antes de confiar nele.

## Como a lista é montada

Regra: **nome só propõe, checksum decide.**

Um BPS guarda no rodapé o CRC32 da ROM de origem — é contra ele que o patch
valida antes de aplicar. Para IPS e xdelta, o `readme.txt` traz o MD5 do dump
exigido. O scan já calcula CRC32 e MD5 de cada ROM sua. Então "este patch
serve em algum arquivo meu?" tem resposta numérica exata, sem nome nenhum.

Isso importa porque os dois lados falam vocabulários diferentes, mantidos por
gente diferente. O RetroAchievements arquiva 117 hacks sob "Pokémon: FireRed
and LeafGreen Versions"; o repositório tem pastas separadas "Pokemon FireRed"
e "Pokemon LeafGreen". Não discordam só na grafia — discordam em quantos jogos
existem.

Então o app baixa um arquivo por pasta candidata em segundo plano, lê o
checksum de origem e guarda o resultado para sempre (`patch-facts.json`). A
seleção de candidatos é deliberadamente generosa: um download de 250 KB
desperdiçado custa muito menos que um hack que você nunca fica sabendo que
existe.

**A impressão digital só soma, nunca subtrai.** Ela resgata hacks que o nome
deixou passar; não remove nenhum que o nome achou. Quando o checksum diz que o
patch precisa de outro dump — outra região ou revisão de um jogo que você tem —
o hack continua na lista marcado como `needs another dump`, com o nome do
arquivo que falta. Esconder seria perder informação: é um hack de um jogo seu,
só falta um arquivo específico. O filtro "Base ROM ready" serve para quando
você quiser ver só o que dá para instalar agora.

A primeira passada é longa. `--verify` roda ela em primeiro plano, por SSH,
sem a interface.


Dois estágios, e a separação é o ponto: o primeiro é de graça, o segundo custa
um download.

**Estágio 1 — quais jogos você tem.** Cada arquivo em `/roms` recebe quatro
impressões digitais: CRC32, MD5 do arquivo inteiro, MD5 sem header (NES, SNES,
PC Engine) e o hash no formato do RetroAchievements. O hash do RA identifica o
jogo base contra a lista de jogos do console — **uma chamada de API por
sistema, não por ROM**. Daí sai o título oficial, que é cruzado com o nome da
pasta no RAPatches.

**Estágio 2 — qual ROM exatamente.** Ao abrir um hack, o patch é baixado e o
**checksum embutido nele** decide qual arquivo local é o dump certo.

O estágio 1 pode ser otimista. O estágio 2 não pode errar.

## Por que o checksum do patch, e não o readme

Todo arquivo do RAPatches traz um `readme.txt` dizendo qual ROM base usar.
Ele é uma pista, não a verdade: numa amostra de 30 arquivos, o
`28343-SMW-SteamboatMario` aponta "Block Kuzushi GB (Japan)" como base de um
hack de Super Mario World.

O footer de um BPS guarda o CRC32 do arquivo de origem, e é contra ele que o
patch vai de fato conferir. Então o casamento usa o checksum do patch primeiro
e só cai no readme para IPS e xdelta, que não carregam checksum nenhum. Nesses
dois casos a única garantia é a comparação final contra o hash do RA.

## Formatos de patch

| Formato | Como é tratado |
| --- | --- |
| BPS | Go puro, valida origem e destino por CRC32 |
| IPS | Go puro, inclusive registros RLE e truncamento |
| UPS | Go puro |
| xdelta | `bin/xdelta3`, binário aarch64 estático incluído (com LZMA) |

Imagens de disco são patcheadas **em streaming**, direto de arquivo para
arquivo. Uma faixa de PS1 tem ~600 MB e carregar origem e destino na memória
ao mesmo tempo fazia o kernel matar o app no meio da instalação — só um
"Killed" no terminal. O pico agora é de alguns kilobytes, qualquer que seja o
tamanho do disco.

Na amostra de 30 arquivos: BPS em 23, IPS em 6, xdelta em 2.

## Índice de patches

O repositório é lido pela árvore do GitHub — uma chamada cobre o catálogo
inteiro, cacheada por 24h. O app já vem com `data/rapatches-index.json`
pronto, então a primeira tela aparece sem depender de rede e sem gastar o
limite de 60 chamadas por hora que o GitHub dá a requisições anônimas.

Para regenerar: `go run ./cmd/seed-index data/rapatches-index.json`.

Hacks por console no índice atual (2.688 no total): SNES 859, N64 427, GBA
401, NES 317, Mega Drive 236, NDS 143, Game Boy 77, GBC 67, PlayStation 63,
GameCube 55, Master System 17, 32X 7.

Duas armadilhas na estrutura do repositório que o parser trata:

- `Removed/<Console>/<Categoria>/` guarda patches retirados, com exatamente o
  mesmo formato de caminho de um hack vivo. Sem a lista de exclusão eles
  apareceriam como instaláveis.
- Dez arquivos usam a categoria no singular (`Hack` em vez de `Hacks`). Sem
  normalizar, esses dez sumiriam da lista.

## O que veio do PoC do itch.io

Sem alteração: `internal/appui` (modelos de tela), `internal/sdlui`
(renderizador SDL2 + `bridge.c`), `internal/rahub` (hash no padrão rcheevos e
cliente da API), `internal/roms`, `internal/text`, `internal/media`.

Novo: `internal/patch`, `internal/rapatches`, `internal/library`,
`internal/catalog`.

## Registrar no menu

    ./install-tool.sh                        # Tools e Ports
    TARGET_DIR=/roms/ports ./install-tool.sh # só um deles

Cria `RA Hack.sh` nos dois lugares, apontando para onde o programa está — não
precisa mover a pasta. Ports costuma estar dois botões mais perto que Tools
nesses menus, e ter o mesmo lançador nos dois não custa nada. Lançadores das
versões antigas (`ROM Hacks.sh`) são removidos.

Depois, atualize a lista de jogos no EmulationStation (ou reinicie) para o
item aparecer.

## Metadata no EmulationStation

Ao instalar, além de salvar as imagens, o app grava a entrada do hack no
`gamelist.xml` do sistema: nome, descrição, capa e gênero. Salvar as imagens
sozinho não basta — assim que um sistema tem gamelist scrapado, o
EmulationStation confia nele e para de adivinhar por nome de arquivo, então um
hack largado na pasta depois aparece em branco.

O arquivo é editado como **texto**, de propósito. Ler e regravar como estrutura
descartaria silenciosamente tudo o que este app não modela — nota, publisher,
contagem de partidas, caminho de vídeo — e um gamelist scrapado tem tudo isso
por jogo. O conteúdo existente sai byte a byte igual; só entra um bloco novo.

Para hacks instalados antes desta versão: `--metadata` reescreve as entradas
de tudo que já está instalado.

## Imagens de reserva do RetroAchievements

Quando um set não tem arte própria, o RetroAchievements devolve uma imagem
dele mesmo no lugar — entre elas um painel cinza escrito "No Screenshot
Found". São imagens válidas: baixam e decodificam perfeitamente, então nada
lá embaixo consegue distingui-las de arte de verdade. Apareciam na galeria com
a cara de uma falha deste app.

São reconhecidas pelo id: as de reserva são os arquivos de numeração mais
baixa do servidor de mídia, enquanto todo upload real tem id de seis dígitos
muito acima. Tratadas como ausência, a galeria cai no ícone da lista.

## Formatos de imagem

png, jpeg, gif e **webp** (VP8 lossy e VP8L lossless, com alfa). O servidor de
mídia do RetroAchievements negocia formato, e o cabeçalho `Accept` anuncia
exatamente o que este build decodifica — anunciar avif e webp sem saber lê-los
fazia cada capa negociada chegar e ser descartada como corrompida, em jogos
cujas imagens estão lá no site.

webp animado não é lido pelo decodificador em Go puro; falha limpo e a página
passa para a imagem seguinte. avif exigiria dav1d via cgo e ficou de fora.

## Imagens

Ao instalar, o app busca as quatro imagens que o RetroAchievements tem do
set (ícone, tela de título, screenshot e box art) e grava em
`/roms/<sistema>/images/`, no padrão que o EmulationStation procura:

    Nome do Hack-image.png    capa (box art, ou tela de título quando não há)
    Nome do Hack-thumb.png    ícone
    Nome do Hack-title.png    tela de título
    Nome do Hack-ingame.png   screenshot

Arquivo que já existe não é sobrescrito — imagem que você escolheu vale mais
que a nossa. A capa também aparece na página do hack dentro do app.

## ROMs dentro de zip

O scanner abre `.zip` e lê a ROM de dentro, porque muita coleção guarda tudo
zipado e os emuladores leem assim mesmo. Os hashes descrevem sempre a ROM, não
o container. Arcade é a exceção: ali o zip **é** a ROM e o RetroAchievements
gera o hash a partir do nome do arquivo, então esses nunca são abertos.

## Quando uma pasta aparece vazia

`--scan` termina explicando o que ignorou, inclusive arquivo grande demais para
ser cartucho. Imagens de disco não têm limite de tamanho: elas nunca são lidas,
são identificadas pelo nome e conferidas depois do patch.


## Controles

| Botão | Na lista |
| --- | --- |
| A | abre o hack |
| B | sai |
| L1 / R1 | muda a ordenação |
| L2 / R2 | passa pelos sistemas |
| X | página de downloads (lá: Y joga, A apaga, X abre a página do hack) |
| Y | limpa todos os filtros |
| Select | filtros (busca, sistema, jogo base, ordem, mostrar) |
| Start | ajustes |

Segurando L1/R1 a lista corre sozinha e acelera: 10 por passo, 25 depois de
um segundo, 60 depois de dois, 120 depois de três. Um toque continua pulando
só 10.

| Botão | Na página do hack |
| --- | --- |
| A | instala (só depois que o patch foi resolvido) |
| X | gerencia/apaga, quando já está instalado |
| Y | joga o hack, quando já está instalado |
| SELECT | notas: o que o hack muda |
| setas | passa para o hack anterior/seguinte da lista |
| L1/R1 | passa pelas imagens do RA |
| L2/R2 | rola a descrição |
| B | volta |

Sair pede confirmação.

Os filtros oferecem só o que este aparelho tem: os sistemas que geraram
hacks e os jogos base que você realmente possui.

## Arquivos grandes de PlayStation

Um xdelta feito contra um disco de 600 MB é grande: três hacks de PS1 no
repositório passam de 70 MB (Resident Evil 3 Hard Mode, CTR Lolo Racing GP,
Spyro 3.5). A mediana de PS1 é 7,7 MB, mas esses três existem.

Por isso o arquivo é baixado **para um arquivo em disco**, não para a memória,
e dentro dele só os itens pequenos (readme, cue, patch de cartucho) são
expandidos de imediato. O que passa de 8 MB fica no disco até alguém pedir, e
aí só aquele é lido.

São dois tetos diferentes de propósito: **1 GB para o download em disco**
(o cartão tem 74 GB) e 64 MB para o que de fato fica na memória. Um teto só,
compartilhado, deixaria a checagem em segundo plano puxar um giga para dentro
de um giga de RAM.

O teto de memória escolhe **estratégia, não se o arquivo é checado**: passando
dele, a checagem estaciona o arquivo no cartão e lê de lá. Isso importa para o
que vem: os 143 hacks de Nintendo DS ainda não foram olhados, e um patch feito
contra um cartucho de 512 MB não é pequeno. Um teto que pulasse esses mandaria
justamente eles de volta para o casamento por nome.

Os arquivos temporários vão para `/roms/.rom-hacks-tmp`, no cartão — **não**
em `/tmp`, que em várias distros de handheld é tmpfs. Escrever lá seria
escrever na RAM que o streaming existe para proteger.

## Um jogo por vez, também na navegação

Abrir a página de um hack baixa o arquivo de patch inteiro e descomprime
tudo que há dentro. Passar rápido por vários jogos deixava uma dúzia desses
downloads no ar ao mesmo tempo, cada um segurando megabytes — e depois de uns
dois minutos o kernel matava o app. Agora abrir outra página cancela o que a
anterior estava buscando, e voltar para a lista solta o arquivo. Uma
instalação em andamento é a exceção: ela é dona do que baixou.

## Progresso da instalação

Uma instalação por vez. A página de qualquer outro hack mostra qual está sendo
aplicado e mantém o A desabilitado até terminar — antes dizia "press A to
patch" e o botão não fazia nada. Quando o patch acaba, o botão volta sozinho,
esteja você na página que estiver. Se você apertou A antes de o patch ficar
pronto, a intenção fica guardada e dispara na vez dele.


Disco de PS1 leva minutos: extrair 600 MB, aplicar o patch, conferir o hash.
A página mostra em que etapa está e a porcentagem do patch. Sair da página e
voltar durante a instalação mostra o progresso em andamento.

## Notas do hack (SELECT na página)

A pergunta "o que esse hack muda exatamente?" quase nunca tem resposta no
RetroAchievements — não existe campo de descrição lá. Mas existe uma fonte
boa que o app já baixava e jogava fora: o **readme dentro do arquivo do
patch**. É o autor escrevendo sobre o próprio hack, muitas vezes com lista de
mudanças, changelog e créditos.

Até agora esse arquivo era lido só para pegar o checksum da ROM base. Agora o
bloco "Use with" e as linhas de hash são removidos e o resto vai para um painel
rolável, com o comentário do RA logo abaixo quando houver. Hack sem readme diz
isso em vez de abrir uma tela vazia.

## Descrição

O RetroAchievements não guarda campo de descrição para um set, então não há
nada oficial para mostrar. O app lê a thread de comentários da página do jogo
e escolhe o post que **melhor descreve** o hack — não o primeiro que passa no
filtro. Ficam de fora automáticos, avisos de claim, links soltos, reações
curtas ("nice set!") e pedidos: "Please make this a set, it's still imo the
best way to play this game" é uma frase sobre o jogo que não descreve nada. Aparece creditado a quem escreveu; é texto de outra
pessoa, não do app.

## Ordenações por popularidade

"Most played (RA)" e "Most unlocks (RA)" precisam de uma chamada por jogo —
não vêm junto com a lista do console. Então são preenchidas em segundo plano,
com pausa entre chamadas, e o que já foi lido fica guardado: a segunda sessão
continua de onde a primeira parou. Jogo sem número ainda lido vai para o fim
da lista, nunca para o começo.

## O que ocupa espaço, e quanto

Nada cresce sem limite. Os caches são proporcionais ao catálogo e às suas
ROMs, não ao tempo de uso:

| Arquivo | Teto |
| --- | --- |
| `rom-library.json` | ~550 KB com 2.200 ROMs |
| `catalog.json` | ~390 KB com 1.000 hacks |
| `patch-facts.json` | ~290 KB com todos os 2.688 patches checados |
| `stats.json` | ~90 KB |
| `installed.json`, `updates.json` | alguns KB |

`/roms/.rom-hacks-tmp/` é a exceção: os arquivos de patch ficam lá enquanto
são lidos e são apagados em seguida. Quando o app morre antes disso — crash,
OOM killer, botão de desligar — eles sobram. Por isso o arranque limpa a
pasta: no arranque nada está baixando, então o que estiver lá é entulho. Só
arquivos com nome `patch-*` são tocados.

## Quando é preciso rodar algo

Só quando você mexe nas ROMs: acrescentou, apagou ou trocou um dump por outro
de região diferente. Pela interface, START → "Rescan /roms"; pelo terminal,
`--refresh`.

O resto se mantém sozinho. O índice de patches é relido a cada 24 horas e,
**quando ele muda, o catálogo é reconstruído na hora** — um hack que ganhou
conjunto de conquistas ontem aparece sem você fazer nada. As impressões
digitais dos patches novos entram na fila logo em seguida, uma vez por patch,
para sempre.

## Checagem a cada 2 dias

Só conta como atualização uma **versão numericamente maior**. Comparar nomes
marcava todo hack instalado como desatualizado, porque o nome do arquivo é
higienizado para o sistema de arquivos e nunca bate exatamente com o do site.
Quando há versão nova, a página diz qual é e o A vira o botão de atualizar.


No arranque, se já passaram 48 horas, o app procura em segundo plano por
hacks novos e confere se os que você instalou ficaram para trás. O segundo
caso é o invisível: o autor lança uma v2.5, o RetroAchievements move o set
para o arquivo novo, e a sua cópia continua abrindo normalmente **sem dar
conquista nenhuma**. Quando isso acontece a lista marca `UPDATE AVAILABLE`.

Custa uma chamada por hack instalado, não por hack listado.

## Limites conhecidos

- **Multi-disco**: quando a ROM é um `.m3u`, o patch é aplicado ao primeiro
  disco que a playlist lista, que é o disco 1 — é contra ele que os hacks são
  feitos.
- **Aviso de região e revisão**: quando o patch pede um dump de outra região,
  ou a mesma região noutra revisão (`(Rev 1)` contra a prensagem original),
  isso aparece na página **antes** de você apertar A, com o nome do arquivo
  que falta. Tendo várias cópias do jogo no cartão, a que casa com o que o
  patch pede é a escolhida — basta acrescentar o dump certo, sem apagar nada. É heurística de nome de arquivo, então só avisa
  quando as duas regiões são conhecidas e diferentes — avisar sobre uma ROM
  que funcionaria seria pior que não avisar.
- **PlayStation precisa do `chdman`** (`sudo apt install mame-tools`). O `.chd`
  é extraído, o `.bin` recebe o patch, o `.cue` que vem no arquivo é gravado
  apontando para ele, e a cópia temporária do track original é apagada. O seu
  `.chd` nunca é tocado. Conta ~1,3 GB de espaço livre durante o processo.
- **Os outros sistemas de disco não têm hack nenhum** no repositório — só
  tradução. Dreamcast 0, Sega CD 0, Saturn 0, PC Engine CD 0, 3DO 0. Por isso
  essas pastas nem são varridas.
- **Ter o jogo não é ter o dump certo.** Se a sua ROM é de outra região ou
  revisão, o app mostra o que o patch quer e qual arquivo seu chegou perto,
  em vez de tentar mesmo assim.
- **Arquivos multipart** (`.7z.001`) não são suportados.
- Os títulos na lista vêm do nome curto do arquivo no repositório, que é
  compacto e nem sempre bonito.

## Testes

```sh
XDELTA3=/caminho/para/xdelta3 go test ./internal/...
```

Os vetores de BPS em `internal/patch/testdata` foram gerados pelo
`python-bps-continued`, uma implementação independente — passar nesses testes
significa concordar com o encoder de outra pessoa, não com as próprias
suposições. Os arquivos em `internal/rapatches/testdata` são archives reais
do repositório, incluindo um com pasta de patches opcionais que precisa ser
ignorada.
